+ [web/s3] the object inspector's CSV, TSV, JSON Lines and Parquet previews scroll the whole file, millions of rows included, in a virtualized data grid
  a worker indexes text files as they stream and reads any block by HTTP Range; Parquet reads only the rows and columns in view
  the grid has a cell cursor, `⌘G` Go to row, `⌘C` copy as TSV, a cell inspector, resizable and hideable columns, and Find over the loaded rows
+ [web/s3] a full-page data viewer at `/s3/<bucket>/view?key=…&row=…` opens a data file with the whole page to scroll it, deep-linked to a row
