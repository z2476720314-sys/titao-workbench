package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"titao/internal/jobs"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil && !errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("prepared-artwork", flag.ContinueOnError)
	flags.SetOutput(out)
	profile := flags.String("profile", "", "profile目录")
	source := flags.String("source", "", "原图路径（不修改）")
	prepared := flags.String("prepared", "", "已经验收的PNG副本")
	expected := flags.String("expected-source-sha", "", "验收时原图SHA256；缺省采用当前快照")
	keepWhole := flags.Bool("keep-whole", false, "保留整幅画面并接受边缘留白")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *profile == "" || *source == "" || *prepared == "" {
		return fmt.Errorf("用法: prepared-artwork --profile DIR --source IMAGE --prepared PNG [--expected-source-sha SHA256]")
	}
	ref, err := jobs.RegisterPreparedArtwork(*profile, *source, *prepared, *expected, *keepWhole)
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(ref)
}
