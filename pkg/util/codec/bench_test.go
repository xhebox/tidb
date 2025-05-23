// Copyright 2016 PingCAP, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package codec

import (
	"testing"
	"time"

	"github.com/pingcap/tidb/pkg/parser/mysql"
	"github.com/pingcap/tidb/pkg/types"
	"github.com/pingcap/tidb/pkg/util/benchdaily"
	"github.com/pingcap/tidb/pkg/util/chunk"
)

var valueCnt = 100

func composeEncodedData(size int) []byte {
	values := make([]types.Datum, 0, size)
	for i := range size {
		values = append(values, types.NewDatum(i))
	}
	bs, _ := EncodeValue(time.UTC, nil, values...)
	return bs
}

func BenchmarkDecodeWithSize(b *testing.B) {
	b.StopTimer()
	bs := composeEncodedData(valueCnt)
	b.StartTimer()
	for range b.N {
		_, err := Decode(bs, valueCnt)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDecodeWithOutSize(b *testing.B) {
	b.StopTimer()
	bs := composeEncodedData(valueCnt)
	b.StartTimer()
	for range b.N {
		_, err := Decode(bs, 1)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEncodeIntWithSize(b *testing.B) {
	for range b.N {
		data := make([]byte, 0, 8)
		EncodeInt(data, 10)
	}
}

func BenchmarkEncodeIntWithOutSize(b *testing.B) {
	for range b.N {
		EncodeInt(nil, 10)
	}
}

func BenchmarkDecodeDecimal(b *testing.B) {
	dec := &types.MyDecimal{}
	err := dec.FromFloat64(1211.1211113)
	if err != nil {
		b.Fatal(err)
	}
	precision, frac := dec.PrecisionAndFrac()
	raw, _ := EncodeDecimal([]byte{}, dec, precision, frac)
	b.ResetTimer()
	for range b.N {
		_, _, _, _, err := DecodeDecimal(raw)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDecodeOneToChunk(b *testing.B) {
	str := new(types.Datum)
	*str = types.NewStringDatum("a")
	var raw []byte
	raw = append(raw, bytesFlag)
	raw = EncodeBytes(raw, str.GetBytes())
	intType := types.NewFieldType(mysql.TypeLonglong)
	b.ResetTimer()
	decoder := NewDecoder(chunk.New([]*types.FieldType{intType}, 32, 32), nil)
	for range b.N {
		_, err := decoder.DecodeOne(raw, 0, intType)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func TestBenchDaily(t *testing.T) {
	benchdaily.Run(
		BenchmarkDecodeWithSize,
		BenchmarkDecodeWithOutSize,
		BenchmarkEncodeIntWithSize,
		BenchmarkEncodeIntWithOutSize,
		BenchmarkDecodeDecimal,
		BenchmarkDecodeOneToChunk,
	)
}
