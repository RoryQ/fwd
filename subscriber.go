package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"github.com/thejerf/suture/v4"
	"io"
	"net/http"
)

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
		client: &http.Client{ },
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
