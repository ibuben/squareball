// Package netguard — HTTP-клиент для запросов по адресам из недоверенных
// источников (картинки в письмах): только публичные IP, проверка после DNS
// на каждом соединении, в том числе после редиректов.
package netguard

import (
	"errors"
	"net"
	"net/http"
	"syscall"
	"time"
)

var ErrPrivate = errors.New("address is in a private network")

var cgnat = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

// Public — адрес из публичного интернета.
func Public(ip net.IP) bool {
	return !(ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() || ip.IsInterfaceLocalMulticast() || cgnat.Contains(ip))
}

// Client — клиент с таймаутом; allowPrivate отключает проверку (только для тестов и стендов).
func Client(timeout time.Duration, allowPrivate bool) *http.Client {
	d := &net.Dialer{Timeout: 10 * time.Second}
	if !allowPrivate {
		d.Control = func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			if ip := net.ParseIP(host); ip == nil || !Public(ip) {
				return ErrPrivate
			}
			return nil
		}
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DialContext = d.DialContext
	tr.Proxy = nil
	tr.MaxIdleConnsPerHost = 4
	return &http.Client{
		Timeout:   timeout,
		Transport: tr,
		CheckRedirect: func(_ *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			return nil
		},
	}
}
