// Copyright 2019-2021 The Liqo Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package errdefs

import (
	"errors"
	"fmt"
)

// ErrInvalidInput is an error interface which denotes whether the opration failed due
// to a the resource not being found.
type ErrInvalidInput interface {
	InvalidInput() bool
	error
}

type invalidInputError struct {
	error
}

func (e *invalidInputError) InvalidInput() bool {
	return true
}

func (e *invalidInputError) Cause() error {
	return e.error
}

// AsInvalidInput wraps the passed in error to make it of type ErrInvalidInput
//
// Callers should make sure the passed in error has exactly the error message
// it wants as this function does not decorate the message.
func AsInvalidInput(err error) error {
	if err == nil {
		return nil
	}
	return &invalidInputError{err}
}

// InvalidInput makes an ErrInvalidInput from the provided error message.
func InvalidInput(msg string) error {
	return &invalidInputError{errors.New(msg)}
}

// InvalidInputf makes an ErrInvalidInput from the provided error format and args.
func InvalidInputf(format string, args ...interface{}) error {
	return &invalidInputError{fmt.Errorf(format, args...)}
}

// IsInvalidInput determines if the passed in error is of type ErrInvalidInput
//
// This will traverse the causal chain (`Cause() error`), until it finds an error
// which implements the `InvalidInput` interface.
func IsInvalidInput(err error) bool {
	if err == nil {
		return false
	}
	if e, ok := err.(ErrInvalidInput); ok {
		return e.InvalidInput()
	}

	if e, ok := err.(causal); ok {
		return IsInvalidInput(e.Cause())
	}

	return false
}
