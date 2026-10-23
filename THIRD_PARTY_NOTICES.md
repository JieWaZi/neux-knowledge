# Third-party notices

neux-knowledge is based on [Tencent WeKnora](https://github.com/Tencent/WeKnora), slimmed down and lightly adapted for a standalone Go module. It directly reuses Wiki storage and models, chunking, extraction and consolidation, citations, links, indexes, prompts, maintenance tools, and related tests. Local changes provide SQLite persistence, managed source files, version and authorization checks, and host model integration.

WeKnora official website: [weknora.weixin.qq.com](https://weknora.weixin.qq.com/).

Project additions are covered by the root MIT LICENSE. The WeKnora copyright and permission notice below applies to the corresponding reused code.

## Additional reuse in API integration

From Tencent/WeKnora commit `8776a2d92d11ad3e76865d51cf3192e37e2a9976`, graph subset selection, graph sorting/BFS and their 13 tests were copied from `internal/handler/wiki_page.go` and the corresponding graph tests into `internal/wiki/graph.go` and `graph_test.go`. The core graph projection, SQLite metadata read port, service detection and HTTP integration/native-upload clients are local adaptations. The original MIT notice below is retained. Tests of remote request/response fixtures are newly written contract tests, not copied end-to-end service tests.

## Tencent WeKnora

The following is the WeKnora copyright and MIT permission notice, excerpted from the upstream license.

```text
Tencent is pleased to support the open source community by making  WeKnora available. 

Copyright (C) 2026 Tencent.  All rights reserved. 

The open-source software included in this distribution may have been modified by Tencent (“Tencent Modifications”). All Tencent Modifications are Copyright (C) Tencent. 



Terms of the MIT:
--------------------------------------------------------------------
Permission is hereby granted, free of charge, to any person obtaining
a copy of this software and associated documentation files (the
"Software"), to deal in the Software without restriction, including
without limitation the rights to use, copy, modify, merge, publish,
distribute, sublicense, and/or sell copies of the Software, and to
permit persons to whom the Software is furnished to do so, subject to
the following conditions:

The above copyright notice and this permission notice shall be
included in all copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND,
EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF
MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT.
IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY
CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT,
TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH THE
SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.
```
