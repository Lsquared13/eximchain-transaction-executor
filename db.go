package main

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path"
	"path/filepath"
	"text/tabwriter"
	"time"

	bolt "github.com/coreos/bbolt"
)

type BoltDB struct {
	*bolt.DB
	userBucket []byte
}

func (db *BoltDB) open(name string) error {
	var err error

	dir, err := filepath.Abs(filepath.Dir(os.Args[0]))
	if err != nil {
		log.Fatal(err)
	}

	db.DB, err = bolt.Open(path.Join(dir, name), 0600, &bolt.Options{Timeout: 1 * time.Second})
	if err != nil {
		return err
	}

	db.userBucket = []byte("users")

	err = db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(db.userBucket)

		if err != nil {
			return errors.New("create user bucket error")
		}

		return nil
	})

	return err
}

func (db *BoltDB) close() error {
	err := db.DB.Close()
	return err
}

func (db *BoltDB) createUser(email string) (string, error) {
	if len(email) == 0 {
		return "", errors.New("user email is empty")
	}

	var token string

	err := db.DB.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(db.userBucket)
		t, err := createToken()
		if err != nil {
			return err
		}

		token = t
		return b.Put([]byte(token), []byte(email))
	})

	return token, err
}

func createToken() (string, error) {
	b := make([]byte, 32)
	_, err := rand.Read(b)
	if err != nil {
		return "", err
	}

	return base64.URLEncoding.EncodeToString(b), err
}

func (db *BoltDB) getUser(token string) (string, error) {
	email := ""
	k := []byte(token)

	err := db.DB.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(db.userBucket)
		v := b.Get(k)
		email = string(v)
		return nil
	})

	return email, err
}

func (db *BoltDB) getTokenByEmail(email string) (string, error) {
	token := ""

	err := db.DB.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(db.userBucket)
		c := b.Cursor()

		for k, v := c.First(); k != nil; k, v = c.Next() {
			if string(v) == email {
				token = string(k)
				return nil
			}
		}

		return nil
	})

	return token, err
}

func (db *BoltDB) listUsers(out io.Writer) error {
	err := db.DB.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(db.userBucket)
		c := b.Cursor()

		w := tabwriter.NewWriter(out, 0, 0, 4, ' ', 0)

		for k, v := c.First(); k != nil; k, v = c.Next() {
			fmt.Fprintf(w, "%s\t%s\n", v, k)
		}

		w.Flush()

		return nil
	})

	return err
}

func (db *BoltDB) deleteUserByToken(token string) error {
	k := []byte(token)

	err := db.DB.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(db.userBucket)
		return b.Delete(k)
	})

	return err
}
