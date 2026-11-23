package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/andreabreu76/mem-engine/internal/config"
	"github.com/andreabreu76/mem-engine/internal/doctor"
)

func runDoctor(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		fmt.Fprintf(stderr, "mem doctor: unexpected arguments\n\n%s", usage)
		return 2
	}
	home, err := config.HomeDir()
	if err != nil {
		fmt.Fprintf(stderr, "mem doctor: %v\n", err)
		return 1
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(stderr, "mem doctor: %v\n", err)
		return 1
	}
	exe, err := os.Executable()
	if err != nil {
		exe = os.Args[0]
	}
	cfg, err := config.LoadFrom(home)
	if err != nil {
		cfg = config.Config{Home: home}
	}
	env := doctor.Env{
		Cfg:        cfg,
		UserHome:   userHome,
		LookPath:   exec.LookPath,
		Executable: exe,
		Now:        time.Now(),
	}
	code := 0
	for _, c := range doctor.Run(context.Background(), env) {
		fmt.Fprintf(stdout, "%-4s  %-14s  %s\n", c.Level, c.Name, c.Detail)
		if c.Level == doctor.Fail {
			code = 1
		}
	}
	return code
}
