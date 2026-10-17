Feature: Simulating the recommender
  A simulation runs the same recommender that issues hints against the current
  pool and the site's policy, from the station's position or from a what-if
  location. It issues nothing, publishes nothing and changes nothing.

  Background:
    Given site "SITE-1" has these locations:
      | code | x  | y | z |
      | L0   | 0  | 0 | 0 |
      | A    | 3  | 0 | 0 |
      | B    | 3  | 4 | 0 |
      | C    | 10 | 0 | 0 |
    And site "SITE-2" has these locations:
      | code | x | y | z |
      | S2A  | 1 | 0 | 0 |
    And site "SITE-1" declares a policy with max detour 25 m, up to 3 candidates and a 90 s hint ttl for "PICK"
    And task "T0" of type "PICK" is available at site "SITE-1" from "L0" to "L0"
    And station "STN-1" claims task "T0"
    And task "T-1" of type "PICK" is available at site "SITE-1" from "A" to "A"
    And task "T-2" of type "PICK" is available at site "SITE-1" from "B" to "B"
    And task "T-3" of type "PICK" is available at site "SITE-1" from "C" to "C"

  Scenario: A simulation from the recorded position ranks like the hint
    Given task "T0" is completed
    When I simulate for station "STN-1"
    Then the simulation lists "T-1,T-2,T-3"
    And the simulation is for site "SITE-1" from "L0" under policy version 1

  Scenario: A simulation issues nothing and publishes nothing
    Given task "T0" is completed
    When I simulate for station "STN-1"
    Then 1 hint was issued
    And the published events are "InterleavingPolicyDeclared,HintIssued"
    And station "STN-1" has an active hint listing "T-1,T-2,T-3"

  Scenario: A what-if location changes the ranking
    Given task "T0" is completed
    When I simulate for station "STN-1" from "C"
    Then the simulation lists "T-3,T-1,T-2"
    And the simulation is for site "SITE-1" from "C" under policy version 1

  Scenario: A what-if location works for a station the service never saw
    When I simulate for station "STN-9" from "C"
    Then the simulation lists "T-3,T-1,T-2"
    And station "STN-9" is unknown

  Scenario: A simulation with no candidate is an empty list, not an error
    Given I declare the policy of site "SITE-1" expecting version 1 with max detour 1 m, up to 3 candidates and a 90 s hint ttl for "PICK"
    When I simulate for station "STN-9" from "L0"
    Then the simulation lists nothing

  Scenario: A site without a policy cannot be simulated
    When I simulate for station "STN-9" from "S2A"
    Then the command fails with "no interleaving policy"

  Scenario: A station without a position needs a what-if location
    When I simulate for station "STN-1"
    Then the command fails with "no recorded position"

  Scenario: An origin without geometry cannot be simulated
    Given the location "U" of site "SITE-1" is registered without geometry
    When I simulate for station "STN-1" from "U"
    Then the command fails with "no known geometry"

  Scenario: An origin the service never saw cannot be simulated
    When I simulate for station "STN-9" from "NOWHERE"
    Then the command fails with "no known geometry"

  Scenario: A station id must be valid
    When I simulate for station "bad id"
    Then the command fails with "invalid station id"
