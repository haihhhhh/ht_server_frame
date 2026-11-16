// Package clusterconfig 校验启动时的集群地址，不连接 NATS 或 etcd。
package clusterconfig

import (
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
)

const etcdURLScheme = "http"

// ValidLocalEndpoints 要求 NATS 和 etcd 都指向回环 IP。
func ValidLocalEndpoints(nats, etcd string) bool {
	if !validNATSEndpoints(nats, false) {
		return false
	}
	endpoint, err := url.Parse(etcd)
	return err == nil && validEtcdEndpoint(endpoint, false)
}

// IsLoopbackListener 检查本机网关的监听 IP。端口范围由基础配置统一校验。
func IsLoopbackListener(address string) bool {
	host, _, err := net.SplitHostPort(address)
	return err == nil && net.ParseIP(host).IsLoopback()
}

// ValidatePrivateEndpoints 允许回环或私网 IP。错误中不包含地址或凭据。
func ValidatePrivateEndpoints(nats, etcd string) error {
	if !validNATSEndpoints(nats, true) {
		return errors.New("app: invalid private NATS endpoints")
	}
	endpoint, err := url.Parse(etcd)
	if err != nil {
		return errors.New("app: invalid private cluster endpoint")
	}
	if !validEtcdEndpoint(endpoint, true) {
		return errors.New("app: private cluster requires isolated private IP endpoints")
	}
	return nil
}

func validEtcdEndpoint(endpoint *url.URL, allowPrivate bool) bool {
	return endpoint.Scheme == etcdURLScheme && endpoint.User == nil && validAddress(endpoint, allowPrivate)
}

// 多个 NATS 地址以逗号分隔，校验后仍将原配置交给 Pitaya。
func validNATSEndpoints(raw string, allowPrivate bool) bool {
	addresses := strings.Split(raw, ",")
	if len(addresses) > 16 {
		return false
	}
	for _, address := range addresses {
		endpoint, err := url.Parse(strings.TrimSpace(address))
		if err != nil || (endpoint.Scheme != "nats" && endpoint.Scheme != "tls") || !validAddress(endpoint, allowPrivate) {
			return false
		}
		if endpoint.User != nil {
			password, ok := endpoint.User.Password()
			if endpoint.User.Username() == "" || !ok || password == "" {
				return false
			}
		}
	}
	return true
}

func validAddress(endpoint *url.URL, allowPrivate bool) bool {
	ip := net.ParseIP(endpoint.Hostname())
	port, err := strconv.Atoi(endpoint.Port())
	return (ip.IsLoopback() || allowPrivate && ip.IsPrivate()) &&
		err == nil && port >= 1 && port <= 65535 &&
		endpoint.Path == "" && endpoint.RawQuery == "" && endpoint.Fragment == ""
}
