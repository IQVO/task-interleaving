Feature: A site without a policy is inert
  No hint is ever issued for a site that has not declared an interleaving
  policy: positions are recorded, nothing is advised, nothing is published.

  Background:
    Given site "SITE-1" has these locations:
      | code | x | y | z |
      | L0   | 0 | 0 | 0 |
      | A    | 3 | 0 | 0 |
    And task "T0" of type "PICK" is available at site "SITE-1" from "L0" to "L0"
    And station "STN-1" claims task "T0"
    And task "T-1" of type "PICK" is available at site "SITE-1" from "A" to "A"

  Scenario: A completion records the position and issues no hint
    When task "T0" is completed
    Then the consumer outcome is "applied"
    And station "STN-1" stands at "L0"
    And station "STN-1" has no active hint
    And 0 hints were issued
    And no event is published

  Scenario: A policy of another site does not wake this site up
    Given site "SITE-2" declares a policy with max detour 25 m, up to 3 candidates and a 90 s hint ttl for "PICK"
    When task "T0" is completed
    Then station "STN-1" has no active hint
    And 0 hints were issued

  Scenario: A policy declared later applies to later completions only
    Given task "T0" is completed
    When site "SITE-1" declares a policy with max detour 25 m, up to 3 candidates and a 90 s hint ttl for "PICK"
    Then station "STN-1" has no active hint
    When 10 seconds pass
    And task "T9" of type "PICK" is available at site "SITE-1" from "L0" to "L0"
    And station "STN-1" claims task "T9"
    And task "T9" is completed
    Then station "STN-1" has an active hint listing "T-1"
    And the published events are "InterleavingPolicyDeclared,HintIssued"
