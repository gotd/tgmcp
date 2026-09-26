package main

import (
	"testing"
)

// TestLoadConfigRightsAreOptIn pins down that every right defaults to off and
// is granted only by the exact string "true".
func TestLoadConfigRightsAreOptIn(t *testing.T) {
	vars := []struct {
		env string
		get func(Config) bool
	}{
		{"TG_ALLOW_SEND", func(c Config) bool { return c.AllowSend }},
		{"TG_ALLOW_PROFILE_EDIT", func(c Config) bool { return c.AllowProfileEdit }},
		{"TG_ALLOW_INLINE_MEDIA", func(c Config) bool { return c.AllowInlineMedia }},
	}
	values := []struct {
		value string
		want  bool
	}{
		{"", false},
		{"true", true},
		{"false", false},
		{"1", false},
		{"TRUE", false},
	}

	for _, v := range vars {
		for _, val := range values {
			t.Run(v.env+"="+val.value, func(t *testing.T) {
				t.Setenv("APP_ID", "1")
				t.Setenv("APP_HASH", "hash")
				for _, other := range vars {
					t.Setenv(other.env, "")
				}
				t.Setenv(v.env, val.value)

				cfg, err := LoadConfig()
				if err != nil {
					t.Fatalf("LoadConfig: %v", err)
				}
				if got := v.get(cfg); got != val.want {
					t.Errorf("%s=%q granted=%v, want %v", v.env, val.value, got, val.want)
				}
			})
		}
	}
}

// TestLoadConfigS3IsAllOrNothing checks that half a bucket configuration is
// rejected. Falling back to the local HTTP store instead would hand out URLs
// nothing can reach.
func TestLoadConfigS3IsAllOrNothing(t *testing.T) {
	for _, tt := range []struct {
		name             string
		endpoint, bucket string
		wantErr          bool
	}{
		{name: "neither"},
		{name: "both", endpoint: "s3.example.com", bucket: "blobs"},
		{name: "endpoint only", endpoint: "s3.example.com", wantErr: true},
		{name: "bucket only", bucket: "blobs", wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("APP_ID", "1")
			t.Setenv("APP_HASH", "hash")
			t.Setenv("TG_BLOB_S3_ENDPOINT", tt.endpoint)
			t.Setenv("TG_BLOB_S3_BUCKET", tt.bucket)

			_, err := LoadConfig()
			if tt.wantErr && err == nil {
				t.Fatal("LoadConfig: want error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("LoadConfig: %v", err)
			}
		})
	}
}
