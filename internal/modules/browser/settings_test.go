package browser

import (
	"slices"
	"testing"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
)

func TestCleanProfile(t *testing.T) {
	p := &inabav1.BrowserProfile{Flags: []string{" --lang=ko ", "", "disable-gpu"}}
	if err := cleanProfile(p); err != nil || !slices.Equal(p.Flags, []string{"lang=ko", "disable-gpu"}) {
		t.Fatalf("cleaned: %v %v", p.Flags, err)
	}
	for _, bad := range []string{"user-data-dir=/tmp/x", "--remote-debugging-port=9222", "Bad Name", "proxy-server=a\nb"} {
		if err := cleanProfile(&inabav1.BrowserProfile{Flags: []string{bad}}); err == nil {
			t.Errorf("%q was taken", bad)
		}
	}
}

func TestPlanFor(t *testing.T) {
	s := &service{settings: &inabav1.BrowserSettings{
		Defaults: &inabav1.BrowserProfile{Flags: []string{"lang=en", "disable-gpu"}},
		Projects: map[string]*inabav1.BrowserProfile{"p": {Sharing: inabav1.BrowserSharing_BROWSER_SHARING_PROJECT, Flags: []string{"lang=ko"}}},
	}}
	got := s.planFor(&inabav1.Thread{Id: "t1", ProjectId: "p"})
	if got.key != "project-p" || !slices.Equal(got.flags, []string{"disable-gpu", "lang=ko"}) {
		t.Fatalf("a sharing project's thread: %+v", got)
	}
	if got := s.planFor(&inabav1.Thread{Id: "t2", ProjectId: "q"}); got.key != "t2" || !slices.Equal(got.flags, []string{"lang=en", "disable-gpu"}) {
		t.Fatalf("another project's thread: %+v", got)
	}
	if got := s.planFor(&inabav1.Thread{Id: "t3"}); got.key != "t3" {
		t.Fatalf("a thread outside projects: %+v", got)
	}
}
