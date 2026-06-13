# File Manager Instructions

You have the file-manager skill. Follow these workspace conventions for all file operations.

## Directory Structure

- **input/** — Contains source files provided by the user. Treat as read-only. Never modify, rename, or delete files in this directory.
- **output/** — Write all results and deliverables here. Use descriptive filenames that indicate the content.
- **scratch/** — Use for temporary or intermediate files during processing. These may be cleaned up automatically.

## Rules

1. **Always organize output**: Place finished work in `output/`. Never leave results scattered in the workspace root.
2. **Preserve input**: Read from `input/` but never write to it. If you need to transform an input file, write the result to `output/`.
3. **Use scratch for temp work**: Intermediate processing steps, drafts, and temporary files belong in `scratch/`.
4. **Check before overwriting**: Before writing to a path that already exists, check if the file is present. If it is, either use a different name (append a timestamp or sequence number) or confirm the overwrite is intended.
5. **Report results**: After completing file operations, summarize what was created or modified, including file paths and sizes.
