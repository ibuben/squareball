package password

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// PwnedURL — адрес диапазонного API Have I Been Pwned. Переменная — для тестов.
var PwnedURL = "https://api.pwnedpasswords.com/range/"

// Breached проверяет пароль по базе утечек через k-anonymity: наружу уходят
// только первые 5 символов SHA-1, сам пароль и полный хеш не покидают сервер.
func Breached(ctx context.Context, client *http.Client, pw string) (bool, error) {
	sum := sha1.Sum([]byte(pw))
	h := strings.ToUpper(hex.EncodeToString(sum[:]))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, PwnedURL+h[:5], nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Add-Padding", "true")
	req.Header.Set("User-Agent", "webmail-password-check")
	res, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return false, fmt.Errorf("pwned passwords: HTTP %d", res.StatusCode)
	}
	sc := bufio.NewScanner(io.LimitReader(res.Body, 4<<20))
	for sc.Scan() {
		suffix, count, ok := strings.Cut(strings.TrimSpace(sc.Text()), ":")
		// Строки-заполнители (Add-Padding) приходят со счётчиком 0.
		if ok && suffix == h[5:] && count != "0" {
			return true, nil
		}
	}
	return false, sc.Err()
}
