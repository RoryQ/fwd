package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/thejerf/suture/v4"
	"io/ioutil"
	"net"
	"net/http"
	"time"
)

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
