package ui

import "time"

func timeAfter() <-chan time.Time { return time.After(50 * time.Millisecond) }
