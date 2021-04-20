package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/thejerf/suture/v4"
	"io/ioutil"
	"os"
	"strconv"
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

type configuration struct {
	Routes map[string]string
}

func parseConfig() configuration {
	bytes, err := ioutil.ReadFile(configPathArg)
	config := configuration{}
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

func debugf(format string, args ...interface{}) {
	if debugMode() {
		fmt.Printf(format+"\n", args...)
	}
}

func infof(format string, args ...interface{}) {
	fmt.Printf(format+"\n", args...)
}

