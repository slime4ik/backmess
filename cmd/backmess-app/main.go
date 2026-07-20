// backmess-app — нативный десктоп-клиент (Fyne + Pion + Opus).
// Сборка: go build -tags nolibopusfile ./cmd/backmess-app
package main

import "github.com/slime4ik/backmess/internal/desktop"

func main() {
	desktop.Run()
}
