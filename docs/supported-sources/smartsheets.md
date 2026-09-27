# Smartsheet

[Smartsheet](https://www.smartsheet.com/) is a software as a service (SaaS) offering for collaboration and work management.

ingestr supports Smartsheet as a source.

## URI format

The URI format for Smartsheet is as follows:

```plaintext
smartsheet://?access_token=<access_token>&smartsheet_id=<sheet_id>
```

URI parameters:

- `access_token` (required): Your Smartsheet API access token.
- `smartsheet_id` (optional): A default sheet ID. It is only used when
  `--source-table` is set to the literal value `sheet`. A numeric or
  `sheet:<id>` value passed to `--source-table` always wins.

The URI is used to connect to the Smartsheet API for extracting data. You can generate an access token in Smartsheet by navigating to Account > Personal Settings > API Access.

## Setting up a Smartsheet Integration

To set up a Smartsheet integration, you'll need an API Access Token.

1. Log in to Smartsheet.
2. Click on "Account" in the bottom left corner, then "Personal Settings".
3. Go to the "API Access" tab.
4. Click "Generate new access token".
5. Give your token a name and click "OK".
6. Copy the generated token. This will be your `access_token`.

The sheet to ingest is identified by its `sheet_id`. You can find the `sheet_id` by opening the sheet in Smartsheet and going to File > Properties. The Sheet ID will be listed there.

`--source-table` accepts three forms:

| Form | Behaviour |
| --- | --- |
| `<sheet_id>` | Use the value as the sheet ID. |
| `sheet:<sheet_id>` | Strip the `sheet:` prefix and use the rest as the sheet ID. |
| `sheet` | Use the `smartsheet_id` URI parameter. Errors if it isn't set. |

A literal value (`<sheet_id>` or `sheet:<sheet_id>`) always wins over the URI parameter — the URI's `smartsheet_id` is only consulted when `--source-table` is `sheet`.

Let's say your access token is `YOUR_ACCESS_TOKEN` and the sheet ID is `1234567890123456`. Here's a sample command using `--source-table`:

```sh
ingestr ingest \
    --source-uri 'smartsheet://?access_token=YOUR_ACCESS_TOKEN' \
    --source-table '1234567890123456' \
    --dest-uri 'duckdb:///smartsheet_data.duckdb' \
    --dest-table 'des.my_sheet_data'
```

Equivalently, with the sheet ID baked into the URI and the `sheet` alias used for `--source-table`:

```sh
ingestr ingest \
    --source-uri 'smartsheet://?access_token=YOUR_ACCESS_TOKEN&smartsheet_id=1234567890123456' \
    --source-table 'sheet' \
    --dest-uri 'duckdb:///smartsheet_data.duckdb' \
    --dest-table 'des.my_sheet_data'
```

The result of this command will be a `my_sheet_data` table containing data from your Smartsheet in the `smartsheet_data.duckdb` database.

> [!CAUTION]
> Smartsheet integration does not currently support incremental loading. Every time you run the command, the entire sheet will be copied from Smartsheet to the destination. This can be slow for large sheets. 