// Copyright (c) 2026 kk
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package runner

import (
	"net/url"
	"testing"
)

func TestNormalizeRTMPURL(t *testing.T) {
	cases := []struct {
		in, path, frag string
	}{
		{"rtmp://h/live/stream1", "/live", "stream1"},
		{"rtmp://h:1935/app/a/b", "/app/a", "b"},
		{"rtmp://h/live#stream1", "/live", "stream1"},
		{"rtmp://h/live", "/live", ""},
		{"rtmps://u:p@h/app/key", "/app", "key"},
	}
	for _, ca := range cases {
		u, err := url.Parse(ca.in)
		if err != nil {
			t.Fatal(err)
		}
		got := normalizeRTMPURL(u)
		if got.Path != ca.path || got.Fragment != ca.frag {
			t.Fatalf("%s → path=%q frag=%q want path=%q frag=%q", ca.in, got.Path, got.Fragment, ca.path, ca.frag)
		}
	}
}

func TestParseRTMPURLRejectsBad(t *testing.T) {
	if _, err := parseRTMPURL("http://x/live/a"); err == nil {
		t.Fatal("want scheme error")
	}
	if _, err := parseRTMPURL("rtmp:///live/a"); err == nil {
		t.Fatal("want host error")
	}
}
