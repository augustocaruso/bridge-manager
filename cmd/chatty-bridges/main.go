package main

import (
	"fmt"
	"os"

	"github.com/beeper/bridge-manager/internal/chatty"
)

func main() {
	config, err := chatty.DefaultConfig()
	if err == nil {
		err = chatty.NewApp(chatty.New(config, os.Stdout)).Run(os.Args)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
