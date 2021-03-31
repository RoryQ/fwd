package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/thejerf/suture/v4"
)

const (
	defaultConfigPath = "~/.config/fwd/fwd.json"
)

var (
	sourceArg, targetArg, configPathArg string
	debugArg                            bool
)

func init() {
	flag.StringVar(&sourceArg, "source", "", "smee.io channel url")
	flag.StringVar(&targetArg, "target", "", "forwarding target")
	flag.StringVar(&configPathArg, "config", defaultConfigPath, "path to config")
	flag.BoolVar(&debugArg, "debug", false, "debug logging")
	flag.Parse()
}

func debugf(format string, args ...interface{}) {
	if debugMode() {
		fmt.Printf(format+"\n", args...)
	}
}

func infof(format string, args ...interface{}) {
	fmt.Printf(format+"\n", args...)
}

func main() {
	ctx := context.Background()
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

	infof("%d routes loaded", c)
	supervisor.Serve(ctx)
}

type Config struct {
	Routes map[string]string
}

func parseConfig() Config {
	bytes, err := ioutil.ReadFile(configPathArg)
	config := Config{}
	if errors.Is(err, os.ErrNotExist) && configPathArg == defaultConfigPath {
		return config
	}

	if err != nil {
		infof("error reading file: %s", err)
		return config
	}

	err = json.Unmarshal(bytes, &config)
	if err != nil {
		infof("error parsing file: %s", err)
	}
	return config
}

func parseTarget() string {
	if t := os.Getenv("FWD_TARGET"); t != "" {
		return t
	}
	return targetArg
}

func parseSource() string {
	if s := os.Getenv("FWD_SOURCE"); s != "" {
		return s
	}
	return sourceArg
}

func debugMode() bool {
	if e := os.Getenv("FWD_DEBUG"); e != "" {
		b, _ := strconv.ParseBool(e)
		return b
	}
	return debugArg
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
	return &Fwder{
		source: source,
		target: target,
		client: &http.Client{
			Timeout: 5 * time.Second,
			Transport: &http.Transport{
				DialContext: (&net.Dialer{
					// This is the TCP connect timeout in this instance.
					Timeout: 2500 * time.Millisecond,
				}).DialContext,
				TLSHandshakeTimeout: 2500 * time.Millisecond,
			},
		},
		stop: make(chan interface{}),
	}
}

type Fwder struct {
	source string
	target string
	client *http.Client

	stop chan interface{}
}

func (f *Fwder) Serve(ctx context.Context) error {
	sub := NewSubscription(f.source)
	name := fmt.Sprintf("Fwder for %s to %s", f.source, f.target)
	infof(name)
	super := suture.NewSimple(name)
	super.Add(sub)
	super.ServeBackground(ctx)

	for {
		select {
		case event := <-sub.Events:
			f.Forward(event)
		case <-f.stop:
			sub.Stop()
			return suture.ErrTerminateSupervisorTree
		}
	}
}

func (f *Fwder) Stop() {
	f.stop <- nil
}

func (f *Fwder) Forward(ev SSEvent) {
	if ev.Name == "ping" || ev.Id == "" || ev.Id == "0" {
		debugf("Skipping received event: %s", ev.Format())
		return
	}

	infof("Received event: %s", ev.Format())

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
		infof(err.Error())
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode > 299 {
		b, _ := ioutil.ReadAll(resp.Body)
		debugf("response code %s: %s", resp.Status, string(b))
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

func (ev SSEvent) Format() string {
	return fmt.Sprintf("id=%v, name=%v, payload=%v", ev.Id, ev.Name, string(ev.Data))
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

func (s *Subscription) Serve(ctx context.Context) error {
	req, _ := http.NewRequest("GET", s.url, nil)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}

	if resp.StatusCode != 200 {
		return fmt.Errorf("Error: resp.StatusCode == %d\n", resp.StatusCode)
	}

	if resp.Header.Get("Content-Type") != "text/event-stream" {
		return fmt.Errorf("Error: invalid Content-Type == %s\n", resp.Header.Get("Content-Type"))
	}

	var buf bytes.Buffer
	ev := SSEvent{}
	s.bodyToClose = resp.Body
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 512*1024), 512*1024)
	for scanner.Scan() {
		select {
		case <-s.stop:
			return suture.ErrTerminateSupervisorTree
		default:
			if err := s.parseSend(scanner.Bytes(), &buf, &ev); err != nil {
				return err
			}
		}
	}

	if err := scanner.Err(); err != nil {
		infof("%s: scanner.Text(): %s", err, scanner.Text())
		return fmt.Errorf("error during resp.Body read: %w", err)
	}

	return nil
}

// parseSend will build the event and when complete send and reset the buffer
func (s *Subscription) parseSend(line []byte, buf *bytes.Buffer, ev *SSEvent) error {
	debugf("len: %d line: %s", len(line), string(line))

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
		return fmt.Errorf("error during EventReadLoop - Default triggered! len:%d\n%s", len(line), line)
	}

	return nil
}
