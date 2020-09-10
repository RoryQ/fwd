package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"strconv"

	"gopkg.in/thejerf/suture.v2"
)

const (
	defaultConfigPath = "~/.config/fwd/fwd.json"
)

var (
	sourceArg     = flag.String("source", "", "smee.io channel url")
	targetArg     = flag.String("target", "", "forwarding target")
	configPathArg = flag.String("config", defaultConfigPath, "path to config")
	debugArg      = flag.Bool("debug", false, "debug logging")
)

func main() {
	flag.Parse()
	supervisor := suture.NewSimple("Supervisor")

	var c int

	s, t := parseSource(), parseTarget()
	if s != "" && t != "" {
		// single target mode
		fwd := NewFwder(parseSource(), parseTarget())
		supervisor.Add(fwd)
		c += 1
	}

	config := parseConfig()
	for k, v := range config.Routes {
		supervisor.Add(NewFwder(k, v))
		c += 1
	}

	fmt.Printf("%d routes loaded\n", c)
	supervisor.Serve()
}

type Config struct {
	Routes map[string]string
}

func parseConfig() Config {
	bytes, err := ioutil.ReadFile(*configPathArg)
	config := Config{}
	if errors.Is(err, os.ErrNotExist) && *configPathArg == defaultConfigPath {
		return config
	}

	if err != nil {
		fmt.Printf("error reading file: %s\n", err)
		return config
	}

	err = json.Unmarshal(bytes, &config)
	if err != nil {
		fmt.Printf("error parsing file: %s\n", err)
	}
	return config
}

func parseTarget() string {
	if t := os.Getenv("FWD_TARGET"); t != "" {
		return t
	}
	return *targetArg
}

func parseSource() string {
	if s := os.Getenv("FWD_SOURCE"); s != "" {
		return s
	}
	return *sourceArg
}

func debugMode() bool {
	if e := os.Getenv("FWD_DEBUG"); e != "" {
		b, _ := strconv.ParseBool(e)
		return b
	}
	return *debugArg
}

func createSmeeChannel() (string, error) {
	httpClient := http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := httpClient.Head("https://smee.io/new")
	if err != nil {
		return "", err
	}

	loc := resp.Header.Get("Location")
	return loc, nil
}

func NewFwder(source, target string) *Fwder {
	fmt.Printf("Forwarding source %s to target %s\n", source, target)
	return &Fwder{
		source: source,
		target: target,
		client: http.DefaultClient,
		stop:   make(chan interface{}),
	}
}

type Fwder struct {
	source string
	target string
	client *http.Client

	stop chan interface{}
}

func (f *Fwder) Serve() {
	sub := NewSubscription(f.source)
	super := suture.NewSimple("Fwder for " + f.source)
	super.Add(sub)
	super.ServeBackground()

	for {
		select {
		case event := <-sub.Events:
			f.Forward(event)
		case <-f.stop:
			sub.Stop()
			return
		}
	}
}

func (f *Fwder) Stop() {
	f.stop <- nil
}

func (f *Fwder) Forward(ev SSEvent) {
	if ev.Name == "ping" || ev.Id == "" {
		if *debugArg {
			fmt.Printf("Received event: id=%v, name=%v, payload=%v\n", ev.Id, ev.Name, string(ev.Data))
		}
		return
	}

	fmt.Printf("Received event: id=%v, name=%v, payload=%v\n", ev.Id, ev.Name, string(ev.Data))

	var p Payload
	json.Unmarshal(ev.Data, &p)

	req, _ := http.NewRequest("POST", f.target, ioutil.NopCloser(bytes.NewReader(p.Body)))
	req.Header.Add("content-type", p.ContentType)
	req.Header.Add("x-request-id", p.XRequestID)
	req.Header.Add("x-github-delivery", p.XGithubDelivery)
	req.Header.Add("x-github-event", p.XGithubEvent)
	req.Header.Add("x-hub-signature", p.XHubSignature)

	resp, err := f.client.Do(req)
	if err != nil {
		fmt.Println(err)
	}
	defer resp.Body.Close()

	if debugMode() || resp.StatusCode > 299 {
		b, _ := ioutil.ReadAll(resp.Body)
		fmt.Printf("response code %s: %s\n", resp.Status, string(b))
	}
}

type Payload struct {
	Host            string
	Connection      string
	UserAgent       string `json:"user-agent"`
	AcceptEncoding  string `json:"accept-encoding"`
	Accept          string
	ContentType     string `json:"content-type"`
	XRequestID      string `json:"x-request-id"`
	XGithubDelivery string `json:"x-github-delivery"`
	XGithubEvent    string `json:"x-github-event"`
	XHubSignature   string `json:"x-hub-signature"`
	Body            json.RawMessage
	Timestamp       int64
}

type SSEvent struct {
	Id   string
	Name string
	Data []byte
}

type Subscription struct {
	Events chan SSEvent
	client *http.Client
	url    string
	stop   chan interface{}

	// response body to be closed when restarting the service
	bodyToClose io.Closer
}

func NewSubscription(url string) *Subscription {
	return &Subscription{
		Events: make(chan SSEvent),
		client: &http.Client{},
		url:    url,
		stop:   make(chan interface{}),
	}
}

func (s *Subscription) Stop() {
	s.stop <- nil
	s.bodyToClose.Close()
}

func (s *Subscription) Serve() {
	req, _ := http.NewRequest("GET", s.url, nil)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := s.client.Do(req)
	if err != nil {
		panic(err)
	}

	if resp.StatusCode != 200 {
		panic(fmt.Errorf("Error: resp.StatusCode == %d\n", resp.StatusCode))
	}

	if resp.Header.Get("Content-Type") != "text/event-stream" {
		panic(fmt.Errorf("Error: invalid Content-Type == %s\n", resp.Header.Get("Content-Type")))
	}

	var buf bytes.Buffer
	ev := SSEvent{}
	s.bodyToClose = resp.Body
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		select {
		case <-s.stop:
			return
		default:
			s.parseSend(scanner.Bytes(), &buf, &ev)
		}
	}

	if err := scanner.Err(); err != nil {
		panic(fmt.Errorf("error during resp.Body read: %w", err))
	}
}

// parseSend will build the event and when complete send and reset the buffer
func (s *Subscription) parseSend(line []byte, buf *bytes.Buffer, ev *SSEvent) {

	if debugMode() {
		fmt.Printf("len: %d line: %s\n", len(line), string(line))
	}

	switch {

	// start of event
	case bytes.HasPrefix(line, []byte("id:")):
		ev.Id = string(line[4:])

	// event name
	case bytes.HasPrefix(line, []byte("event:")):
		ev.Name = string(line[7:])

	// event data
	case bytes.HasPrefix(line, []byte("data:")):
		buf.Write(line[6:])

	// end of event
	case len(line) == 0:
		ev.Data = buf.Bytes()
		buf.Reset()
		s.Events <- *ev
		ev = &SSEvent{}

	default:
		//fmt.Fprintf(os.Stderr, "error during EventReadLoop - Default triggered! len:%d\n%s", len(line), line)
		panic(fmt.Errorf("error during EventReadLoop - Default triggered! len:%d\n%s", len(line), line))
	}
}
