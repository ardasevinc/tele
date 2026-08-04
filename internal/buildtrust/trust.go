package buildtrust

import "errors"

const (
	Identifier  = "com.ardasevinc.tele"
	TeamID      = "J3S8HNBXSU"
	Requirement = `identifier "com.ardasevinc.tele" and anchor apple generic and certificate 1[field.1.2.840.113635.100.6.2.6] /* exists */ and certificate leaf[field.1.2.840.113635.100.6.1.13] /* exists */ and certificate leaf[subject.OU] = J3S8HNBXSU`
)

var ErrNotOfficial = errors.New("running Tele build does not satisfy the official signing requirement")
