// Copyright 2015 PingCAP, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// See the License for the specific language governing permissions and
// limitations under the License.

package kv

import (
	"context"
	"errors"
)

// UnionStore is a store that wraps a snapshot for read and a BufferStore for buffered write.
// Also, it provides some transaction related utilities.
type UnionStore interface {
	MemBuffer
	// GetKeyExistErrInfo gets the key exist error info for the lazy check.
	GetKeyExistErrInfo(k Key) *existErrInfo
	// DeleteKeyExistErrInfo deletes the key exist error info for the lazy check.
	DeleteKeyExistErrInfo(k Key)
	// WalkBuffer iterates all buffered kv pairs.
	WalkBuffer(f func(k Key, v []byte) error) error
	// SetOption sets an option with a value, when val is nil, uses the default
	// value of this option.
	SetOption(opt Option, val interface{})
	// DelOption deletes an option.
	DelOption(opt Option)
	// GetOption gets an option.
	GetOption(opt Option) interface{}
	// GetMemBuffer return the MemBuffer binding to this UnionStore.
	GetMemBuffer() MemBuffer
}

// AssertionType is the type of a assertion.
type AssertionType int

// The AssertionType constants.
const (
	None AssertionType = iota
	Exist
	NotExist
)

// Option is used for customizing kv store's behaviors during a transaction.
type Option int

// Options is an interface of a set of options. Each option is associated with a value.
type Options interface {
	// Get gets an option value.
	Get(opt Option) (v interface{}, ok bool)
}

type existErrInfo struct {
	idxName string
	value   string
}

// NewExistErrInfo is used to new an existErrInfo
func NewExistErrInfo(idxName string, value string) *existErrInfo {
	return &existErrInfo{idxName: idxName, value: value}
}

// GetIdxName gets the index name of the existed error.
func (e *existErrInfo) GetIdxName() string {
	return e.idxName
}

// GetValue gets the existed value of the existed error.
func (e *existErrInfo) GetValue() string {
	return e.value
}

// Err generates the error for existErrInfo
func (e *existErrInfo) Err() error {
	return ErrKeyExists.FastGenByArgs(e.value, e.idxName)
}

type unionStoreIter struct {
	key     Key
	val     []byte
	iter    []Iterator
	done    bool
	reverse bool
}

func (usi *unionStoreIter) Valid() bool {
	return !usi.done
}

func (usi *unionStoreIter) Key() Key {
	return usi.key
}

func (usi *unionStoreIter) Value() []byte {
	return usi.val
}

func (usi *unionStoreIter) Next() error {
	for {
		var min Key
		var minIt Iterator

		j := 0
		for i := len(usi.iter) - 1; i >= 0; i-- {
			it := usi.iter[i]

			if !it.Valid() {
				j++
				continue
			}
			if it.Key().Cmp(usi.key) == 0 {
				it.Next()
				if !it.Valid() {
					j++
					continue
				}
			}

			if !usi.reverse {
				if min == nil || min.Cmp(it.Key()) > 0 {
					min = it.Key()
					minIt = it
				}
			} else {
				if min == nil || min.Cmp(it.Key()) < 0 {
					min = it.Key()
					minIt = it
				}
			}
		}

		if j != len(usi.iter) {
			usi.key = min
			usi.val = minIt.Value()
			if minIt.Valid() {
				minIt.Next()
			}
		} else {
			if usi.done {
				return errors.New("no more keys")
			}
			usi.done = true
			return nil
		}

		if len(usi.val) != 0 {
			return nil
		}
	}
}

func (usi *unionStoreIter) Close() {
	usi.done = true
	for i := range usi.iter {
		usi.iter[i].Close()
	}
}

// unionStore is an in-memory Store which contains a buffer for write and a
// snapshot for read.
type unionStore struct {
	retriever    Retriever
	buffers      []MemBuffer
	keyExistErrs map[string]*existErrInfo // for the lazy check
	opts         options
}

// NewUnionStore builds a new UnionStore.
func NewUnionStore(snapshot Snapshot) UnionStore {
	return &unionStore{
		retriever: snapshot,
		// TODO: maybe just keep the current buffer
		buffers:      []MemBuffer{NewMemDbBuffer()},
		keyExistErrs: make(map[string]*existErrInfo),
		opts:         make(map[Option]interface{}),
	}
}

// Get implements the Retriever interface.
func (us *unionStore) Get(ctx context.Context, k Key) ([]byte, error) {
	var v []byte
	var err error

	for i := len(us.buffers) - 1; i >= 0; i-- {
		v, err = us.buffers[i].Get(ctx, k)
		if err == nil {
			break
		}
	}
	if IsErrNotFound(err) {
		if _, ok := us.opts.Get(PresumeKeyNotExists); ok {
			e, ok := us.opts.Get(PresumeKeyNotExistsError)
			if ok {
				us.keyExistErrs[string(k)] = e.(*existErrInfo)
			}
			return nil, ErrNotExist
		}
		v, err = us.retriever.Get(ctx, k)
	}
	if err != nil {
		return v, err
	}
	if len(v) == 0 {
		return nil, ErrNotExist
	}
	return v, nil
}

func (us *unionStore) WalkBuffer(f func(Key, []byte) error) error {
	it, err := us.Iter(nil, nil)
	if err != nil {
		return err
	}

	for it.Valid() {
		err = f(it.Key(), it.Value())
		if err != nil {
			return err
		}

		err = it.Next()
		if err != nil {
			return err
		}
	}

	return nil
}

func (us *unionStore) Iter(k Key, upperBound Key) (Iterator, error) {
	var err error
	iter := make([]Iterator, len(us.buffers)+1)

	iter[0], err = us.retriever.Iter(k, upperBound)
	if err != nil {
		return nil, err
	}

	for i := range us.buffers {
		iter[i+1], err = us.buffers[i].Iter(k, upperBound)
		if err != nil {
			return nil, err
		}
	}

	r := &unionStoreIter{
		iter:    iter,
		done:    false,
		reverse: false,
	}
	return r, r.Next()
}

func (us *unionStore) IterReverse(k Key) (Iterator, error) {
	var err error
	iter := make([]Iterator, len(us.buffers)+1)

	iter[0], err = us.retriever.IterReverse(k)
	if err != nil {
		return nil, err
	}

	for i := range us.buffers {
		iter[i+1], err = us.buffers[i].IterReverse(k)
		if err != nil {
			return nil, err
		}
	}

	r := &unionStoreIter{
		iter:    iter,
		done:    false,
		reverse: true,
	}
	return r, r.Next()
}

func (us *unionStore) Size() int {
	r := 0
	for i := range us.buffers {
		r += us.buffers[i].Size()
	}
	return r
}

func (us *unionStore) Len() int {
	r := 0
	for i := range us.buffers {
		r += us.buffers[i].Len()
	}
	return r
}

func (us *unionStore) Set(k Key, v []byte) error {
	return us.buffers[len(us.buffers)-1].Set(k, v)
}

func (us *unionStore) Delete(k Key) error {
	// It is ok to just delete it here
	// delete(k) = set(k, nil) internally
	// merge will empty this entrie
	return us.buffers[len(us.buffers)-1].Delete(k)
}

func (us *unionStore) GetKeyExistErrInfo(k Key) *existErrInfo {
	if c, ok := us.keyExistErrs[string(k)]; ok {
		return c
	}
	return nil
}

func (us *unionStore) DeleteKeyExistErrInfo(k Key) {
	delete(us.keyExistErrs, string(k))
}

// SetOption implements the UnionStore SetOption interface.
func (us *unionStore) SetOption(opt Option, val interface{}) {
	us.opts[opt] = val
}

// DelOption implements the UnionStore DelOption interface.
func (us *unionStore) DelOption(opt Option) {
	delete(us.opts, opt)
}

// GetOption implements the UnionStore GetOption interface.
func (us *unionStore) GetOption(opt Option) interface{} {
	return us.opts[opt]
}

// GetMemBuffer return the MemBuffer binding to this UnionStore.
func (us *unionStore) GetMemBuffer() MemBuffer {
	return us.buffers[len(us.buffers)-1]
}

func (us *unionStore) NewStagingBuffer() MemBuffer {
	ret := us.buffers[len(us.buffers)-1].NewStagingBuffer()
	us.buffers = append(us.buffers, ret)
	return ret
}

func (us *unionStore) Flush() (int, error) {
	r, e := us.buffers[len(us.buffers)-1].Flush()
	if e == nil {
		us.buffers = us.buffers[:len(us.buffers)-1]
	}
	return r, e
}

func (us *unionStore) Discard() {
	us.buffers[len(us.buffers)-1].Discard()
	us.buffers = us.buffers[:len(us.buffers)-1]
}

type options map[Option]interface{}

func (opts options) Get(opt Option) (interface{}, bool) {
	v, ok := opts[opt]
	return v, ok
}
