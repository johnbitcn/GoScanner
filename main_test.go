package main

import (
	"bytes"
	"context"
	"fmt"
	"testing"
)

func TestCLIValidation(t *testing.T) {
	cases := []struct {
		args []string
		code int
	}{
		{nil, 0}, {[]string{"help"}, 0}, {[]string{"--help"}, 0},
		{[]string{"scan", "-h"}, 0}, {[]string{"unknown"}, 2},
		{[]string{"scan", "--dpi", "bad"}, 2}, {[]string{"scan", "--dpi", "0"}, 2},
		{[]string{"scan", "--mode", "bad"}, 2}, {[]string{"scan", "--mode", "bw"}, 2}, {[]string{"scan", "--area", "NaN,0,1,1"}, 2},
		{[]string{"info", "--host", "http://192.168.1.8"}, 2},
		{[]string{"info", "--timeout", "0s"}, 2}, {[]string{"info", "extra"}, 2},
		{[]string{"scan", "-o", "not-jpeg.png"}, 1},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprint(tc.args), func(t *testing.T) {
			var out, stderr bytes.Buffer
			if code := run(context.Background(), tc.args, &out, &stderr); code != tc.code {
				t.Fatalf("exit=%d, expected=%d; %s", code, tc.code, stderr.String())
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if exitCode(ctx) != 130 || exitCode(context.Background()) != 1 {
		t.Fatal("wrong cancellation exit code")
	}
}
