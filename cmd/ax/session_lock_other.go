//go:build !unix

package main

import "os"

func lockFile(*os.File) error { return nil }

func unlockFile(*os.File) {}

func syncDir(string) {}
