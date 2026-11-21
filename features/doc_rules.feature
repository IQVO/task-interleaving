Feature: Documentation rules of the third wave: the default page, the candidate payload and order-independent ranking
  The use-case catalogue fixes a default listing limit of 100 (limit 1 to 500).
  The CloudEvents payload table says every candidate of a hint carries its task
  type and the location it starts at. The v1 policy computes a detour rounded to
  0.1 m, and the max-detour limit checks that rounded value. The recommender's
  answer depends only on the pool's contents, never on the order the
  announcements arrived in. A superseded hint raises no event of its own.

  Background:
    Given site "SITE-1" has these locations:
      | code | x | y | z |
      | L0   | 0 | 0 | 0 |
      | A    | 3 | 0 | 0 |
      | B    | 3 | 4 | 0 |
    And site "SITE-1" declares a policy with max detour 25 m, up to 3 candidates and a 90 s hint ttl for "PICK"
    And task "T0" of type "PICK" is available at site "SITE-1" from "L0" to "L0"
    And station "STN-1" claims task "T0"
    And task "T-1" of type "PICK" is available at site "SITE-1" from "A" to "A"
    And task "T-2" of type "PICK" is available at site "SITE-1" from "B" to "B"

  Scenario: A listing without a limit shows the newest 100 hints and pages the rest
    Given 101 stations complete a task that ends at "L0" of site "SITE-1"
    When I list the hints with the default limit
    Then the page holds 100 hints
    And the first hint of the page is number 101
    And the page has a next cursor
    When I list the hints with limit 10 after the cursor of the previous page
    Then the page holds 1 hint
    And the first hint of the page is number 1
    And the page has no next cursor

  Scenario: A hint's candidates carry their task type and the location they start at
    Given site "SITE-2" has these locations:
      | code | x | y | z |
      | S2O  | 0 | 0 | 0 |
      | S2A  | 3 | 0 | 0 |
      | S2B  | 6 | 0 | 0 |
    And site "SITE-2" declares a policy with max detour 25 m, up to 3 candidates and a 90 s hint ttl for "PICK, REPLENISH, RELOCATE"
    And task "T2-0" of type "PICK" is available at site "SITE-2" from "S2O" to "S2O"
    And station "STN-2" claims task "T2-0"
    And task "T2-pick" of type "PICK" is available at site "SITE-2" from "S2A" to "S2A"
    And task "T2-rep" of type "REPLENISH" is available at site "SITE-2" from "S2B" to "S2B"
    When task "T2-0" is completed
    Then station "STN-2" has an active hint listing "T2-pick,T2-rep"
    And candidate 1 of the active hint of station "STN-2" is task "T2-pick" of type "PICK" at location "S2A"
    And candidate 2 of the active hint of station "STN-2" is task "T2-rep" of type "REPLENISH" at location "S2B"

  Scenario: The detour limit checks the rounded detour, 25.04 m is 25.0 m and qualifies, 25.06 m does not
    Given site "SITE-3" has these locations:
      | code  | x     | y | z |
      | S3O   | 0     | 0 | 0 |
      | S3IN  | 25.04 | 0 | 0 |
      | S3OUT | 25.06 | 0 | 0 |
    And site "SITE-3" declares a policy with max detour 25 m, up to 3 candidates and a 90 s hint ttl for "PICK"
    And task "T3-0" of type "PICK" is available at site "SITE-3" from "S3O" to "S3O"
    And station "STN-3" claims task "T3-0"
    And task "T3-in" of type "PICK" is available at site "SITE-3" from "S3IN" to "S3IN"
    And task "T3-out" of type "PICK" is available at site "SITE-3" from "S3OUT" to "S3OUT"
    When task "T3-0" is completed
    Then station "STN-3" has an active hint listing "T3-in"
    And candidate 1 of the active hint of station "STN-3" is "T3-in" with detour 25 m

  Scenario: The hint is the same whatever order the announcements arrived in
    Given site "SITE-A" has these locations:
      | code | x | y | z |
      | A-O  | 0 | 0 | 0 |
      | A-N  | 3 | 0 | 0 |
      | A-F  | 3 | 4 | 0 |
    And site "SITE-A" declares a policy with max detour 25 m, up to 3 candidates and a 90 s hint ttl for "PICK"
    And site "SITE-B" has these locations:
      | code | x | y | z |
      | B-O  | 0 | 0 | 0 |
      | B-N  | 3 | 0 | 0 |
      | B-F  | 3 | 4 | 0 |
    And site "SITE-B" declares a policy with max detour 25 m, up to 3 candidates and a 90 s hint ttl for "PICK"
    And task "TA-N" of type "PICK" is available at site "SITE-A" from "A-N" to "A-N"
    And task "TA-F" of type "PICK" is available at site "SITE-A" from "A-F" to "A-F"
    And task "TB-F" of type "PICK" is available at site "SITE-B" from "B-F" to "B-F"
    And task "TB-N" of type "PICK" is available at site "SITE-B" from "B-N" to "B-N"
    And task "TA-0" of type "PICK" is available at site "SITE-A" from "A-O" to "A-O"
    And station "STN-A" claims task "TA-0"
    And task "TB-0" of type "PICK" is available at site "SITE-B" from "B-O" to "B-O"
    And station "STN-B" claims task "TB-0"
    When task "TA-0" is completed
    And task "TB-0" is completed
    Then station "STN-A" has an active hint listing "TA-N,TA-F"
    And station "STN-B" has an active hint listing "TB-N,TB-F"
    And candidate 1 of the active hint of station "STN-A" is "TA-N" with detour 3 m
    And candidate 2 of the active hint of station "STN-A" is "TA-F" with detour 7 m
    And candidate 1 of the active hint of station "STN-B" is "TB-N" with detour 3 m
    And candidate 2 of the active hint of station "STN-B" is "TB-F" with detour 7 m

  Scenario: A superseded hint raises no event of its own
    Given task "T-3" of type "PICK" is available at site "SITE-1" from "B" to "B"
    And station "STN-1" claims task "T-3"
    And task "T0" is completed
    And 10 seconds pass
    When task "T-3" is completed
    Then the published events are "InterleavingPolicyDeclared,HintIssued,HintIssued"
    And hint number 1 is "Superseded"
    And hint number 2 is "Active"
