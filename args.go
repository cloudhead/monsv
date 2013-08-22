package main

import (
	"fmt"
)

type args []string

func (a *args) Set(s string) error {
	*a = append(*a, s)
	return nil
}

func (a *args) String() string {
	return fmt.Sprint(*a)
}
