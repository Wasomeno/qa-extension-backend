package main

import (
	"context"
	"errors"
	"fmt"
)

func main() {
    err := fmt.Errorf("Agent execution error: context canceled")
    if errors.Is(err, context.Canceled) {
        fmt.Println("Is Canceled")
    } else {
        fmt.Println("Not Canceled")
    }
}
