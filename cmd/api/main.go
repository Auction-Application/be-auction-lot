package main

import (
	"fmt"

	"github.com/joho/godotenv"
)

func main() {
	if err := godotenv.Load(); err != nil {
		fmt.Println("No .env found, when using environment variables")
		fmt.Println(err)
		return
	}

}
