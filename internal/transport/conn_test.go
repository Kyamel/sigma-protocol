package transport

import (
	"errors"
	"net"
	"os"
	"testing"
	"time"
)

func TestPipeDeliversInOrder(t *testing.T) {
	a, b := Pipe(0)
	defer a.Close()
	defer b.Close()

	for _, msg := range []string{"um", "dois", "três"} {
		if _, err := a.WriteTo([]byte(msg), nil); err != nil {
			t.Fatal(err)
		}
	}
	buf := make([]byte, 16)
	for _, want := range []string{"um", "dois", "três"} {
		n, addr, err := b.ReadFrom(buf)
		if err != nil {
			t.Fatal(err)
		}
		if string(buf[:n]) != want || addr.String() != "mem-a" {
			t.Fatalf("recebido %q de %v, esperado %q de mem-a", buf[:n], addr, want)
		}
	}
}

// WriteTo deve copiar o buffer: o chamador pode reutilizá-lo imediatamente.
func TestPipeCopiesBuffer(t *testing.T) {
	a, b := Pipe(0)
	defer a.Close()
	defer b.Close()
	p := []byte("abc")
	a.WriteTo(p, nil)
	p[0] = 'X'
	buf := make([]byte, 3)
	b.ReadFrom(buf)
	if string(buf) != "abc" {
		t.Fatalf("recebido %q", buf)
	}
}

func TestPipeOverflowDrops(t *testing.T) {
	a, b := Pipe(2)
	defer a.Close()
	defer b.Close()
	for range 5 {
		a.WriteTo([]byte("x"), nil)
	}
	if got := drain(t, b, 20*time.Millisecond); len(got) != 2 {
		t.Fatalf("fila de 2 entregou %d", len(got))
	}
}

func TestReadDeadline(t *testing.T) {
	a, b := Pipe(0)
	defer a.Close()
	defer b.Close()

	b.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
	start := time.Now()
	_, _, err := b.ReadFrom(make([]byte, 1))
	var ne net.Error
	if !errors.As(err, &ne) || !ne.Timeout() || !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("esperado timeout, obtido %v", err)
	}
	if time.Since(start) < 15*time.Millisecond {
		t.Fatal("prazo expirou cedo demais")
	}

	// Prazo no passado falha imediatamente; prazo zero remove o limite.
	b.SetReadDeadline(time.Now().Add(-time.Second))
	if _, _, err := b.ReadFrom(make([]byte, 1)); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("prazo no passado: %v", err)
	}
	b.SetReadDeadline(time.Time{})
	a.WriteTo([]byte("ok"), nil)
	if _, _, err := b.ReadFrom(make([]byte, 2)); err != nil {
		t.Fatalf("sem prazo: %v", err)
	}
}

// Alterar o prazo enquanto uma leitura espera deve valer para essa leitura.
func TestDeadlineChangeWakesReader(t *testing.T) {
	a, b := Pipe(0)
	defer a.Close()
	defer b.Close()

	done := make(chan error, 1)
	go func() {
		_, _, err := b.ReadFrom(make([]byte, 1))
		done <- err
	}()
	time.Sleep(10 * time.Millisecond)
	b.SetReadDeadline(time.Now())
	select {
	case err := <-done:
		if !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatalf("esperado timeout, obtido %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("leitura não acordou após mudança de prazo")
	}
}

func TestCloseUnblocksReader(t *testing.T) {
	a, b := Pipe(0)
	defer a.Close()

	done := make(chan error, 1)
	go func() {
		_, _, err := b.ReadFrom(make([]byte, 1))
		done <- err
	}()
	time.Sleep(10 * time.Millisecond)
	b.Close()
	select {
	case err := <-done:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("esperado net.ErrClosed, obtido %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close não desbloqueou a leitura")
	}
	if _, err := b.WriteTo([]byte("x"), nil); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("WriteTo após Close: %v", err)
	}
}
