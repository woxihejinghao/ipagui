package services

import "testing"

func TestAppStoreLinks(t *testing.T) {
	deepLink, webLink := appStoreLinks(414478124)

	if deepLink != "macappstore://apps.apple.com/app/id414478124" {
		t.Fatalf("深链 = %q", deepLink)
	}

	if webLink != "https://apps.apple.com/app/id414478124" {
		t.Fatalf("网页 = %q", webLink)
	}
}
