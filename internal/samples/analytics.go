package samples

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"strconv"
	"time"
)

// analytics.go — the analytics dataset: a month of web-shop orders, as
// Hive-partitioned CSV and as Parquet, with Glue tables over both and an
// Iceberg copy when the engine can make one.
//
// Every value is a function of the order's index, so every load, on every
// machine, writes the same bytes, and docs and screenshots match what a
// reader loads.

const (
	analyticsBucket   = "overcast-sample-analytics"
	analyticsDatabase = "sample_analytics"
	analyticsOrders   = 300
)

// order is one row of the dataset.
type order struct {
	ID         int64
	Date       time.Time
	CustomerID string
	Product    string
	Quantity   int32
	UnitPrice  float64
	Region     string
}

var (
	analyticsRegions  = []string{"us", "eu", "apac"}
	analyticsProducts = []struct {
		name  string
		price float64
	}{
		{"keyboard", 49}, {"mouse", 19.5}, {"monitor", 229}, {"headset", 89.99}, {"webcam", 64.25}, {"dock", 159},
	}
	analyticsStart = time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
)

// analyticsOrderRows are the dataset's orders, in id order.
func analyticsOrderRows() []order {
	rows := make([]order, analyticsOrders)
	for i := range rows {
		p := analyticsProducts[(i*5+i/3)%len(analyticsProducts)]
		rows[i] = order{
			ID:         int64(1001 + i),
			Date:       analyticsStart.AddDate(0, 0, (i*7)%31),
			CustomerID: fmt.Sprintf("c-%03d", 1+(i*37)%60),
			Product:    p.name,
			Quantity:   int32(1 + (i*13)%5),
			UnitPrice:  p.price,
			Region:     analyticsRegions[i%len(analyticsRegions)],
		}
	}
	return rows
}

// analyticsColumns is the tables' columns, less the CSV table's partition.
const analyticsColumns = `order_id bigint, order_date date, customer_id string, product string, quantity int, unit_price double`

func analyticsDataset() dataset {
	rows := analyticsOrderRows()
	csvLocation, parquetLocation := s3URI(analyticsBucket, "csv/orders/"), s3URI(analyticsBucket, "parquet/orders/")
	icebergLocation := s3URI(analyticsBucket, "iceberg/orders/")
	objects := csvPartitions(rows)
	objects = append(objects, object{Key: "parquet/orders/orders.parquet", Body: ordersParquet(rows), ContentType: "application/vnd.apache.parquet"})
	return dataset{
		Name: "analytics", Bucket: analyticsBucket, Database: analyticsDatabase, Objects: objects,
		Statements: []string{
			`CREATE DATABASE IF NOT EXISTS ` + analyticsDatabase + ` COMMENT 'Overcast sample dataset: a month of web-shop orders'`,
			`CREATE EXTERNAL TABLE IF NOT EXISTS ` + analyticsDatabase + `.orders_csv (` + analyticsColumns + `)
PARTITIONED BY (region string)
ROW FORMAT DELIMITED FIELDS TERMINATED BY ','
LOCATION '` + csvLocation + `'
TBLPROPERTIES ('skip.header.line.count' = '1')`,
			`MSCK REPAIR TABLE ` + analyticsDatabase + `.orders_csv`,
			`CREATE EXTERNAL TABLE IF NOT EXISTS ` + analyticsDatabase + `.orders_parquet (` + analyticsColumns + `, region string)
STORED AS PARQUET
LOCATION '` + parquetLocation + `'`,
		},
		Tables: []Table{
			{Name: "orders_csv", Format: "CSV", Location: csvLocation, Partitions: len(analyticsRegions), Rows: len(rows)},
			{Name: "orders_parquet", Format: "PARQUET", Location: parquetLocation, Rows: len(rows)},
		},
		Iceberg: &Table{Name: "orders_iceberg", Format: "ICEBERG", Location: icebergLocation, Rows: len(rows)},
		IcebergSQL: `CREATE TABLE ` + analyticsDatabase + `.orders_iceberg
WITH (table_type = 'ICEBERG', location = '` + icebergLocation + `', is_external = false)
AS SELECT * FROM ` + analyticsDatabase + `.orders_parquet`,
	}
}

// csvPartitions is one CSV file, with a header, per region.
func csvPartitions(rows []order) []object {
	objects := make([]object, 0, len(analyticsRegions))
	for _, region := range analyticsRegions {
		var buf bytes.Buffer
		w := csv.NewWriter(&buf)
		_ = w.Write([]string{"order_id", "order_date", "customer_id", "product", "quantity", "unit_price"})
		for _, o := range rows {
			if o.Region == region {
				_ = w.Write([]string{strconv.FormatInt(o.ID, 10), o.Date.Format(time.DateOnly), o.CustomerID, o.Product,
					strconv.Itoa(int(o.Quantity)), strconv.FormatFloat(o.UnitPrice, 'f', -1, 64)})
			}
		}
		w.Flush()
		objects = append(objects, object{Key: "csv/orders/region=" + region + "/orders.csv", Body: buf.Bytes(), ContentType: "text/csv"})
	}
	return objects
}

// ordersParquet is every order as one Parquet file.
func ordersParquet(rows []order) []byte {
	id := &parquetColumn{name: "order_id", typ: parquetInt64, converted: convertedNone}
	date := &parquetColumn{name: "order_date", typ: parquetInt32, converted: convertedDate}
	customer := &parquetColumn{name: "customer_id", typ: parquetByteArray, converted: convertedUTF8}
	product := &parquetColumn{name: "product", typ: parquetByteArray, converted: convertedUTF8}
	quantity := &parquetColumn{name: "quantity", typ: parquetInt32, converted: convertedNone}
	price := &parquetColumn{name: "unit_price", typ: parquetDouble, converted: convertedNone}
	region := &parquetColumn{name: "region", typ: parquetByteArray, converted: convertedUTF8}
	for _, o := range rows {
		id.putInt64(o.ID)
		date.putInt32(int32(o.Date.Unix() / 86400))
		customer.putString(o.CustomerID)
		product.putString(o.Product)
		quantity.putInt32(o.Quantity)
		price.putDouble(o.UnitPrice)
		region.putString(o.Region)
	}
	return encodeParquet([]*parquetColumn{id, date, customer, product, quantity, price, region}, len(rows))
}

func s3URI(bucket, prefix string) string { return "s3://" + bucket + "/" + prefix }
