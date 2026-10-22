package types

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
)

type StringArray []string

// Value implements the driver.Valuer interface, used to convert StringArray to database value
func (c StringArray) Value() (driver.Value, error) {
	return json.Marshal(c)
}

// Scan implements the sql.Scanner interface, used to convert database value to StringArray
// Scan accepts SQLite JSON TEXT and BLOB; unknown types must not silently become empty arrays.
func (c *StringArray) Scan(value interface{}) error {
	if value == nil {
		*c = nil
		return nil
	}
	var raw []byte
	switch value := value.(type) {
	case []byte:
		raw = value
	case string:
		raw = []byte(value)
	default:
		return fmt.Errorf("invalid JSON array SQL type %T", value)
	}
	return json.Unmarshal(raw, c)
}
