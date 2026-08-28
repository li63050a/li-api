// Package redisc 提供一个极简的纯标准库 Redis 客户端，用于可选的多实例分布式会话。
// 通过环境变量 REDIS_ADDR 启用，未配置时 Enabled() 返回 false，调用方可回退到内存存储。
package redisc

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	initOnce sync.Once
	enabled  bool
	addr     string
	password string

	poolMu sync.Mutex
	pool   []net.Conn

	errNil = errors.New("nil reply")
)

const maxIdle = 8

type respErr string

func (e respErr) Error() string { return string(e) }

func initRedis() {
	addr = os.Getenv("REDIS_ADDR")
	if addr == "" {
		enabled = false
		return
	}
	enabled = true
	password = os.Getenv("REDIS_PASSWORD")
}

// Enabled 报告 Redis 是否已配置启用。
func Enabled() bool {
	initOnce.Do(initRedis)
	return enabled
}

func dial() (net.Conn, error) {
	c, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		return nil, err
	}
	if password != "" {
		if _, err := doOnConn(c, "AUTH", password); err != nil {
			c.Close()
			return nil, err
		}
	}
	if _, err := doOnConn(c, "PING"); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

func getConn() (net.Conn, error) {
	poolMu.Lock()
	if n := len(pool); n > 0 {
		c := pool[n-1]
		pool = pool[:n-1]
		poolMu.Unlock()
		return c, nil
	}
	poolMu.Unlock()
	return dial()
}

func putConn(c net.Conn) {
	poolMu.Lock()
	if len(pool) < maxIdle {
		pool = append(pool, c)
		poolMu.Unlock()
		return
	}
	poolMu.Unlock()
	c.Close()
}

// doOnConn 在指定连接上执行一条 RESP 命令并读取其回复。
func doOnConn(c net.Conn, args ...string) (string, error) {
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	var b strings.Builder
	fmt.Fprintf(&b, "*%d\r\n", len(args))
	for _, a := range args {
		fmt.Fprintf(&b, "$%d\r\n%s\r\n", len(a), a)
	}
	if _, err := c.Write([]byte(b.String())); err != nil {
		return "", err
	}
	return readReply(bufio.NewReader(c))
}

func readReply(r *bufio.Reader) (string, error) {
	t, err := r.ReadByte()
	if err != nil {
		return "", err
	}
	switch t {
	case '+':
		line, err := r.ReadString('\n')
		if err != nil {
			return "", err
		}
		return strings.TrimRight(line, "\r\n"), nil
	case '-':
		line, err := r.ReadString('\n')
		if err != nil {
			return "", err
		}
		return "", respErr(strings.TrimRight(line, "\r\n"))
	case ':':
		line, err := r.ReadString('\n')
		if err != nil {
			return "", err
		}
		return strings.TrimRight(line, "\r\n"), nil
	case '$':
		line, err := r.ReadString('\n')
		if err != nil {
			return "", err
		}
		n, err := strconv.Atoi(strings.TrimRight(line, "\r\n"))
		if err != nil {
			return "", err
		}
		if n == -1 {
			return "", errNil
		}
		buf := make([]byte, n)
		if _, err := io.ReadFull(r, buf); err != nil {
			return "", err
		}
		var crlf [2]byte
		if _, err := io.ReadFull(r, crlf[:]); err != nil {
			return "", err
		}
		return string(buf), nil
	}
	return "", fmt.Errorf("unexpected RESP type: %q", string(t))
}

// do 执行一条单轮往返命令并返回 RESP 回复。连接出错时丢弃并重连一次。
func do(args ...string) (string, error) {
	initOnce.Do(initRedis)
	if !enabled {
		return "", errors.New("redis disabled")
	}
	c, err := getConn()
	if err != nil {
		return "", err
	}
	reply, err := doOnConn(c, args...)
	if err != nil {
		if err == errNil {
			putConn(c)
			return "", errNil
		}
		if _, ok := err.(respErr); ok {
			putConn(c)
			return "", err
		}
		c.Close()
		c, derr := dial()
		if derr != nil {
			return "", derr
		}
		reply, err = doOnConn(c, args...)
		if err != nil {
			c.Close()
			return "", err
		}
		putConn(c)
		return reply, nil
	}
	putConn(c)
	return reply, nil
}

// Set 写入带过期时间的键值（SET key value EX <sec>）。
func Set(key, value string, ttl time.Duration) error {
	_, err := do("SET", key, value, "EX", strconv.FormatInt(int64(ttl/time.Second), 10))
	return err
}

// Get 读取键值；键不存在或出错时 ok=false。
func Get(key string) (string, bool) {
	v, err := do("GET", key)
	if err != nil {
		return "", false
	}
	return v, true
}

// Del 删除键。
func Del(key string) error {
	_, err := do("DEL", key)
	return err
}

// IncrExpire 原子自增并刷新过期时间（用于分布式限流）。
func IncrExpire(key string, ttl time.Duration) (int64, error) {
	v, err := do("INCR", key)
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, err
	}
	if _, err := do("EXPIRE", key, strconv.FormatInt(int64(ttl/time.Second), 10)); err != nil {
		return n, err
	}
	return n, nil
}
