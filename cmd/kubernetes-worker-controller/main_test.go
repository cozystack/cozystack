package main

import "testing"

func TestOptions(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*options)
		want   bool
	}{
		{"defaults", func(*options) {}, true},
		{"empty default", func(o *options) { o.defaultClass = "" }, false},
		{"invalid default", func(o *options) { o.defaultClass = "Bad/Class" }, false},
		{"empty TLS directory", func(o *options) { o.certDir = "" }, false},
		{"invalid port", func(o *options) { o.port = 0 }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := defaultOptions()
			tc.mutate(&o)
			if err := o.validate(); (err == nil) != tc.want {
				t.Fatalf("validation=%v want valid=%v", err, tc.want)
			}
		})
	}
}
