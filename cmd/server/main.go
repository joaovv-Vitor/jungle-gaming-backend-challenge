package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"go.uber.org/fx"

	"github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/bootstrap"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "healthcheck" {
		if err := checkHealth(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	fx.New(bootstrap.Module).Run()
}

func checkHealth() error {
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:8080/health/ready", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("readiness returned %s", resp.Status)
	}
	return nil
}
