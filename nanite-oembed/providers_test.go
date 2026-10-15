package main

import "testing"

func TestMatchProvider(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string
	}{
		{name: "youtube", url: "https://www.youtube.com/watch?v=abc", want: "YouTube"},
		{name: "spotify", url: "https://open.spotify.com/track/123", want: "Spotify"},
		{name: "vimeo", url: "https://vimeo.com/12345", want: "Vimeo"},
		{name: "soundcloud", url: "https://soundcloud.com/user/track", want: "SoundCloud"},
		{name: "flickr", url: "https://www.flickr.com/photos/user/123", want: "Flickr"},
		{name: "unknown", url: "https://example.com", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MatchProvider(tt.url)
			if tt.want == "" {
				if got != nil {
					t.Fatalf("MatchProvider(%q) = %v, want nil", tt.url, got)
				}
				return
			}
			if got == nil || got.Name != tt.want {
				t.Fatalf("MatchProvider(%q) = %v, want %q", tt.url, got, tt.want)
			}
		})
	}
}
