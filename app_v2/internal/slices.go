package internal

func PointerSliceToInterfaceSlice[T any](items []*T) []interface{} {
	res := make([]interface{}, len(items))
	for i, v := range items {
		res[i] = v
	}

	return res
}

func SliceToInterfaceSlice[T any](items []T) []interface{} {
	res := make([]interface{}, len(items))
	for i, v := range items {
		res[i] = v
	}

	return res
}
