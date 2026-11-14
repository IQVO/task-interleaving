Feature: Hint expiry rules of the sweeper
  The hint-expiry worker sweeps Active hints at or past expires_at. Only an Active
  hint expires, each station is expired on its own, and one sweep looks at no more
  than 200 stations.

  Background:
    Given site "SITE-1" has these locations:
      | code | x  | y | z |
      | L0   | 0  | 0 | 0 |
      | A    | 3  | 0 | 0 |
      | D    | 30 | 0 | 0 |
    And site "SITE-1" declares a policy with max detour 25 m, up to 3 candidates and a 90 s hint ttl for "PICK"
    And task "T-1" of type "PICK" is available at site "SITE-1" from "A" to "A"

  Scenario: A sweep expires only the hints that are due and announces each expiry
    Given task "T0" of type "PICK" is available at site "SITE-1" from "L0" to "L0"
    And station "STN-1" claims task "T0"
    And task "T0b" of type "PICK" is available at site "SITE-1" from "L0" to "L0"
    And station "STN-2" claims task "T0b"
    And task "T0" is completed
    And 30 seconds pass
    And task "T0b" is completed
    When 60 seconds pass
    And the expiry sweep runs
    Then the sweep expired 1 hint
    And hint number 1 is "Expired"
    And hint number 2 is "Active"
    And the last HintExpired is of hint number 1 with expiry "2026-10-17T09:01:30Z"
    When 30 seconds pass
    And the expiry sweep runs
    Then the sweep expired 1 hint
    And hint number 2 is "Expired"

  Scenario: A superseded hint is never expired, only the hint that replaced it
    Given task "T0" of type "PICK" is available at site "SITE-1" from "L0" to "L0"
    And station "STN-1" claims task "T0"
    And task "T0" is completed
    And 10 seconds pass
    And task "T5" of type "PICK" is available at site "SITE-1" from "D" to "A"
    And station "STN-1" claims task "T5"
    And task "T5" is completed
    When 100 seconds pass
    And the expiry sweep runs
    Then the sweep expired 1 hint
    And hint number 1 is "Superseded"
    And hint number 2 is "Expired"
    And the published events are "InterleavingPolicyDeclared,HintIssued,HintIssued,HintExpired"

  Scenario: One sweep looks at 200 stations at most, the next sweep takes the rest
    Given 201 stations complete a task that ends at "L0" of site "SITE-1"
    Then 201 hints are in state "Active"
    When 91 seconds pass
    And the expiry sweep runs
    Then the sweep expired 200 hints
    And 1 hint is in state "Active"
    When the expiry sweep runs
    Then the sweep expired 1 hint
    And 0 hints are in state "Active"
