package main

import (
	"fmt"
	"strings"
)

func getBaseURL() string {
	if config.addr != "" {
		if !strings.HasPrefix(config.addr, "http") {
			return fmt.Sprintf("http://%s", config.addr)
		}
		return config.addr
	}
	return fmt.Sprintf("http://%s:%d", config.host, config.port)
}
