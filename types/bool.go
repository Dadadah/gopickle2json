// Copyright 2022 Juniper Networks/Mist Systems. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package types

type Bool bool

func (b Bool) JSON() string {
	if b {
		return "true"
	}
	return "false"
}