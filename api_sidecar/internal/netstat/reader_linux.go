package netstat

func NewReader(root string) Reader { return NewLinuxReader(root) }
