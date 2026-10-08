//go:build !darwin

package main

import "os"

func runMain(fn func() int) { os.Exit(fn()) }
