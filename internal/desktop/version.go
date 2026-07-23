package desktop

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// appVersion — версия этой сборки. Держим в одном месте: отсюда её берут и
// метаданные окна, и проверка обновлений. Перед релизом поднимать вместе с
// тегом (тег vX.Y.Z ↔ эта строка X.Y.Z).
const appVersion = "0.2.1"

const releasesPage = "https://github.com/slime4ik/backmess/releases/latest"
const latestAPI = "https://api.github.com/repos/slime4ik/backmess/releases/latest"

// checkUpdate спрашивает у GitHub последний релиз и сравнивает с appVersion.
// Возвращает (номер новой версии, есть ли обновление). Тихо возвращает false
// при любой ошибке сети — проверка обновлений не должна мешать запуску.
func checkUpdate() (latest string, newer bool) {
	c := &http.Client{Timeout: 8 * time.Second}
	resp, err := c.Get(latestAPI)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", false
	}
	var r struct {
		Tag string `json:"tag_name"`
	}
	if json.NewDecoder(resp.Body).Decode(&r) != nil {
		return "", false
	}
	tag := strings.TrimPrefix(strings.TrimSpace(r.Tag), "v")
	if tag == "" {
		return "", false
	}
	return tag, semverLess(appVersion, tag)
}

// semverLess: a строго меньше b по номерам версий вида X.Y.Z. Недостающие и
// нечисловые части считаем нулём — этого хватает для наших тегов.
func semverLess(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < 3; i++ {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(numPrefix(pa[i]))
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(numPrefix(pb[i]))
		}
		if x != y {
			return x < y
		}
	}
	return false
}

// numPrefix отрезает суффиксы вроде "1-rc2" → "1".
func numPrefix(s string) string {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	return s[:i]
}
