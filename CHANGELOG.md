# Changelog
All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### forked - 2022-10-27
- forked from nlpodyssey/gopickle (or really, a child of that, nsd20463/gopickle
  b/c I wanted the improvements there)

## [0.1.0] - 2021-01-06
### Added
- More and better documentation
- `OrderedDict.MustGet()`
- `Dict.MustGet()`
- `pytorch.LoadWithUnpickler()` which allows loading PyTorch modules using a
  custom unpickler.
- Handle legacy method `torch.nn.backends.thnn._get_thnn_function_backend` when
  loading pytorch modules.

### Changed
- `FrozenSet` implementation was modified, avoiding confusion with `Set`.
- Replace build CI job with tests and coverage
- `Dict` has been reimplemented using a slice, instead of a map, because in Go
  not all types can be map's keys (e.g. slices).
- Use Go version `1.15`

### Removed
- Unused method `List.Extend`

## [0.0.1-alpha.1] - 2020-05-23
### Fixed
- Modify GitHub Action steps `Build` and `Test` including all sub-packages.

## [0.0.1-alpha.0] - 2020-05-23
### Added
- Initial implementation of `types` package
- Initial implementation of `pickle` package
- Initial implementation of `pytorch` package
