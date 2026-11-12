package cfg

import "testing"

func TestWorkspacePathSnapshotsKeepEmptyListsAndDoNotShareStorage(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if s.Get().PatternPaths != nil || s.Get().TemplatePaths != nil {
		t.Fatal("legacy unset paths must remain distinguishable from cleared lists")
	}
	c, err := s.Update(func(c *Config) {
		c.PatternPaths = []string{"pattern"}
		c.TemplatePaths = []string{"template"}
	})
	if err != nil {
		t.Fatal(err)
	}
	c.PatternPaths[0], c.TemplatePaths[0] = "changed", "changed"
	c = s.Get()
	if c.PatternPaths[0] != "pattern" || c.TemplatePaths[0] != "template" {
		t.Fatal("returned configuration shares workspace path storage")
	}
	c.PatternPaths[0], c.TemplatePaths[0] = "changed again", "changed again"
	c = s.Get()
	if c.PatternPaths[0] != "pattern" || c.TemplatePaths[0] != "template" {
		t.Fatal("Get shares workspace path storage")
	}
	if _, err := s.Update(func(c *Config) {
		c.PatternPaths, c.TemplatePaths = []string{}, []string{}
	}); err != nil {
		t.Fatal(err)
	}
	c = s.Get()
	if c.PatternPaths == nil || c.TemplatePaths == nil {
		t.Fatal("cleared path lists became unset legacy defaults")
	}
}
