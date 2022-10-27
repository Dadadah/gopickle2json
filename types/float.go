// Copyright 2022 Juniper Networks/Mist Systems. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package types

import "strconv"

type Float float64

func (f Float) JSON() string {
	return strconv.FormatFloat(float64(f), 'G', -1, 64)
}