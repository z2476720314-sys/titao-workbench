package main

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"titao/internal/jobs"
)

func TestPreparedCLIImportsLocalArtworkAndRequiresExplicitInputs(t *testing.T) {
	dir := t.TempDir()
	profile := filepath.Join(dir, "profile")
	source, approved := filepath.Join(dir, "item.png"), filepath.Join(dir, "approved.png")
	var pngData bytes.Buffer
	im := image.NewNRGBA(image.Rect(0, 0, 2, 3))
	im.SetNRGBA(1, 1, color.NRGBA{R: 120, A: 255})
	if err := png.Encode(&pngData, im); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{source, approved} {
		if err := os.WriteFile(path, pngData.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var output bytes.Buffer
	for _, args := range [][]string{nil, {"--profile", profile}, {"--profile", profile, "--source", source}, {"--profile", profile, "--source", source, "--prepared", approved, "unexpected"}} {
		if err := run(args, &output); err == nil {
			t.Fatal("CLI accepted missing or trailing inputs")
		}
	}
	output.Reset()
	if err := run([]string{"--profile", profile, "--source", source, "--prepared", approved}, &output); err != nil {
		t.Fatal(err)
	}
	var ref jobs.PreparedArtwork
	if err := json.Unmarshal(output.Bytes(), &ref); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(ref.Path, 0600) })
	if err := jobs.ValidatePreparedInput(profile, source, ref.SourceSHA256, ref.Path, ref.PreparedSHA256); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := run([]string{"--profile", profile, "--source", source, "--prepared", approved, "--expected-source-sha", ref.SourceSHA256}, &output); err != nil {
		t.Fatal(err)
	}
	if current, _ := os.ReadFile(source); !bytes.Equal(current, pngData.Bytes()) {
		t.Fatal("CLI modified original artwork")
	}
	output.Reset()
	if err := run([]string{"--profile", profile, "--source", source, "--prepared", approved, "--keep-whole"}, &output); err != nil {
		t.Fatal(err)
	}
	var policy map[string]any
	if err := json.Unmarshal(output.Bytes(), &policy); err != nil || policy["keep_whole"] != true {
		t.Fatal("CLI did not capture the approved whole-image policy")
	}
}
