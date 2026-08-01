package store

// CategoryTotal is spend for one log-entry category.
type CategoryTotal struct {
	Category   string `json:"category"`
	TotalCents int64  `json:"total_cents"`
	Count      int64  `json:"count"`
}

// MonthTotal is spend for one calendar month.
type MonthTotal struct {
	Month      string `json:"month"` // YYYY-MM
	TotalCents int64  `json:"total_cents"`
	Count      int64  `json:"count"`
}

// ItemTotal is spend on one named item, with quantity rolled up.
type ItemTotal struct {
	Item       string  `json:"item"`
	Category   string  `json:"category"`
	TotalCents int64   `json:"total_cents"`
	Quantity   float64 `json:"quantity"`
	Unit       string  `json:"unit"`
	Count      int64   `json:"count"`
}

// VendorTotal is spend with one supplier.
type VendorTotal struct {
	Vendor     string `json:"vendor"`
	TotalCents int64  `json:"total_cents"`
	Count      int64  `json:"count"`
}

// CostFilter scopes a cost query.
type CostFilter struct {
	PoolID   int64
	SeasonID int64
	From     string
	To       string
}

// where builds the shared predicate for cost queries.
func (f CostFilter) where() (string, []any) {
	q := ` WHERE l.pool_id = ?`
	args := []any{f.PoolID}
	if f.SeasonID > 0 {
		q += ` AND l.season_id = ?`
		args = append(args, f.SeasonID)
	}
	if f.From != "" {
		q += ` AND l.occurred_on >= ?`
		args = append(args, f.From)
	}
	if f.To != "" {
		q += ` AND l.occurred_on <= ?`
		args = append(args, f.To)
	}
	return q, args
}

// CostByCategory totals spend per category, largest first.
func (db *DB) CostByCategory(f CostFilter) ([]CategoryTotal, error) {
	where, args := f.where()
	rows, err := db.Query(`SELECT l.category, COALESCE(SUM(l.cost_cents),0), COUNT(*) FROM log_entries l`+where+
		` GROUP BY l.category ORDER BY SUM(l.cost_cents) DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CategoryTotal{}
	for rows.Next() {
		var c CategoryTotal
		if err := rows.Scan(&c.Category, &c.TotalCents, &c.Count); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// CostByMonth totals spend per calendar month, oldest first, so it can be
// drawn as a running total.
func (db *DB) CostByMonth(f CostFilter) ([]MonthTotal, error) {
	where, args := f.where()
	rows, err := db.Query(`SELECT substr(l.occurred_on,1,7) AS m, COALESCE(SUM(l.cost_cents),0), COUNT(*)
	  FROM log_entries l`+where+` GROUP BY m ORDER BY m`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MonthTotal{}
	for rows.Next() {
		var m MonthTotal
		if err := rows.Scan(&m.Month, &m.TotalCents, &m.Count); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// CostByItem totals spend per item name, largest first.
func (db *DB) CostByItem(f CostFilter, limit int) ([]ItemTotal, error) {
	where, args := f.where()
	q := `SELECT l.item, l.category, COALESCE(SUM(l.cost_cents),0), COALESCE(SUM(l.quantity),0),
	  COALESCE(MAX(l.unit),''), COUNT(*) FROM log_entries l` + where +
		` GROUP BY l.item, l.category ORDER BY SUM(l.cost_cents) DESC`
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ItemTotal{}
	for rows.Next() {
		var i ItemTotal
		if err := rows.Scan(&i.Item, &i.Category, &i.TotalCents, &i.Quantity, &i.Unit, &i.Count); err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// CostByVendor totals spend per supplier, largest first.
func (db *DB) CostByVendor(f CostFilter) ([]VendorTotal, error) {
	where, args := f.where()
	rows, err := db.Query(`SELECT l.vendor, COALESCE(SUM(l.cost_cents),0), COUNT(*) FROM log_entries l`+where+
		` AND l.vendor != '' GROUP BY l.vendor ORDER BY SUM(l.cost_cents) DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []VendorTotal{}
	for rows.Next() {
		var v VendorTotal
		if err := rows.Scan(&v.Vendor, &v.TotalCents, &v.Count); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// CostTotal is the headline number for a filter: total spend and entry count.
func (db *DB) CostTotal(f CostFilter) (cents int64, count int64, err error) {
	where, args := f.where()
	err = db.QueryRow(`SELECT COALESCE(SUM(l.cost_cents),0), COUNT(*) FROM log_entries l`+where, args...).Scan(&cents, &count)
	return
}

// TestCadence counts tests per month, for the testing-frequency chart.
func (db *DB) TestCadence(poolID int64) ([]MonthTotal, error) {
	rows, err := db.Query(`SELECT substr(tested_at,1,7) AS m, 0, COUNT(*) FROM tests WHERE pool_id = ?
	  GROUP BY m ORDER BY m`, poolID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MonthTotal{}
	for rows.Next() {
		var m MonthTotal
		if err := rows.Scan(&m.Month, &m.TotalCents, &m.Count); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
