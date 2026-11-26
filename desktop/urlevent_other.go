//go:build !darwin && !linux

package main

func urlEvents() <-chan string { return nil }

func runMain(app func()) { app() }
